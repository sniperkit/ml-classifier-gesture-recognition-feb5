package qprob

/* Methods to build and train the qprob classifier
data structure.  Also includes retraining functionality
to allow single column to be retrained as needed by the
optimizers */

import (
	"bufio"
	"fmt"
	"math"
	"strconv"
	s "strings"
	//"io"
	//"io/ioutil"
	"log"
	//"net/http"
	"os"
	//"os/exec"
)

const (
	QTBucket = 1 // Quantize using bucket logic
	QTInt    = 2 // Quantize with simple int index
	QTText   = 3 // Quantize as tokenized text field
)
const MaxFloat32 = math.MaxFloat32

type QuantType uint8
type TQIndex int16  // type index into Quant Buckets
type TQCnt int32    // type Counts made for Quant Buckets
type TQProb float32 // type for Probability compute

// contains the count for each class
// identfied as having at least one row.
// eg for bucket 10 we have 4 classes.
// each id=1, count=10, id=5, count=4
// totalCnt is the sum of the Counts
// for all classes
type QuantList struct {
	Counts map[int16]int32
	totCnt int32
}

// includes the data and descriptive meta
// data for 1 feature of data or in CSV this
// would be 1 column of data.
type Feature struct {
	Buckets        map[int16]*QuantList
	Spec           *CSVCol
	Enabled        bool
	ProcType       QuantType
	EffMaxVal      float32
	EffMinVal      float32
	EffRange       float32
	BuckSize       float32
	NumBuck        int16
	NumBuckOveride bool
	FeatWeight     float32
	// Need the following as part of feature to support sparse
	// feature matrix where not all rows have all featurs.
	// an example would be text processing where  # feaures
	// is # of unique phrases + # unique tokens but each
	// row only contains a small words.
	ClassCounts map[int16]int32   // total Count of all Records of Class
	ClassProb   map[int16]float32 // Prob of any record being in class
	NumRow      float32           // used to allow sparse feature matrix
}

// Includes the metadaa and classifer
// data for a given classifer
type Classifier struct {
	ColDef       []*Feature
	ColNumByName map[string]int
	ColByName    map[string]*Feature
	Info         *CSVInfo
	TrainFiName  string
	TestFiName   string
	Label        string
	NumCol       int
	NumRow       int
	ClassCol     int
	NumBuck      int16
	ClassCounts  map[int16]int32   // total Count of all Records of Class
	ClassProb    map[int16]float32 // Prob of any record being in class
	NumTrainRow  int32
}

// A set of classifiers indexed by
// name to allow a single process
// to serve data from multiple training
// data sets
type Classifiers struct {
	classifiers map[string]*Classifier
}

/* Determines reasonable effective Min/Max value range for
the statistically significant values that represent the bulk
of the data.  Without this feature the quantize process can
compute poor bucket sizes that are negatively effected by a very
small amoun of data.  We do this by dividing all inbound data
counts between 1,000 buckets across the absolute min/max range
then to find the effective range we scan from the top and bottom
until the next bucket would exteed the numRemove specification
then return the value equivilant to the last bucket on each end
that did not exceed the numRemove requirement.  This allows us
to remove the influence for a given fraction of the data extreeme
value data on each end when computing effective range which
provides a better computation of bucket size based on the dominant
data.  This is not always desirable but in stock data it is very
common to have 1% to 2% with extreeme values which ends up causing
buckets that are too large forcing values in the main distribution
into a smaller number of buckets than intended. If either value
for bottom or top meet each other before reachign the number of
records then returns a error. */
func (fe *Feature) setFeatEffMinMax(fier *Classifier, numRemMin int, numRemMax int) (float32, float32) {
	minCnt := 0
	maxCnt := 0
	csv := fe.Spec
	dist := csv.distCounts
	stepSize := csv.distStepSize

	// Scan from Bottom removing large numbers
	// We look ahead 1 so if it would place us
	// over our targetCnt then we abort.
	bndx := -1
	effMinVal := csv.MinFlt
	for ; numRemMin > (minCnt+dist[bndx+1]) && bndx < 999; bndx++ {
		//fmt.Printf("L144: bndx=%v effMinVal=%v minCnt=%v nextDist=%v numRemMin=%v\n",
		//	bndx, effMinVal, minCnt, dist[bndx+1], numRemMin)
		minCnt += dist[bndx+1]
		effMinVal += stepSize
	}

	// Scan From Top removing large numbers
	// We look back by 1 so if that value would put us
	// over target removed then abort based on curr Val.
	tndx := 1000
	effMaxVal := csv.MaxFlt
	for ; numRemMax > (maxCnt+dist[tndx-1]) && tndx > 1; tndx-- {
		maxCnt += dist[tndx-1]
		//fmt.Printf("L155: tndx=%v effMaxVal=%v maxCnt=%v nextDist=%v numRemMax=%v\n",
		//	tndx, effMaxVal, maxCnt, dist[tndx-1], numRemMax)
		effMaxVal -= stepSize
	}
	if effMaxVal > effMinVal {
		fe.EffMinVal = effMinVal
		fe.EffMaxVal = effMaxVal
		fier.updateStepValues(fe)
	} else {
		//fmt.Printf("ERR effMax < effMin in setFeatEffMinMax")
	}
	return fe.EffMinVal, fe.EffMaxVal
}

// set Min Max Effective Range for every feature.
func (fier *Classifier) SetEffMinMax(numRemMin int, numRemMax int) {
	for cn := 0; cn < fier.NumCol; cn++ {
		fe := fier.ColDef[cn]
		_, _ = fe.setFeatEffMinMax(fier, numRemMin, numRemMax)

		csv := fe.Spec
		fmt.Printf("SetEffMinMax colNum=%v absMin=%v effMin=%v absMax=%v effMax=%v remMin=%v remMax=%v\n",
			cn, csv.MinFlt, fe.EffMinVal, csv.MaxFlt, fe.EffMaxVal, numRemMin, numRemMax)
	}
}

// Remove the top portion of the outlier records
// where portion of set is a ratio expressed between
// 0.0 and 1.0.  Applied to all features.
func (fier *Classifier) SetEffMinMaxPortSet(portSet float32) {
	numRemoveRec := int(float32(fier.NumRow) * portSet)
	fier.SetEffMinMax(numRemoveRec, numRemoveRec)
}

func (fier *Classifier) totFeatWeight() float32 {
	tout := float32(0.0)
	for ndx, feat := range fier.ColDef {
		if feat.Enabled == true && ndx != fier.ClassCol {
			tout += feat.FeatWeight
		}
	}
	return tout
}

/* printClassifierModel */
/* LoadClassifierTrainingModel */

/* For an existing pre-loaded training data sett
add additional traiing data. This will not adjust
the bucket size or effective min, max values used
to compute step size for bucket indexing.   */
/*func AddClassifierTrainingFile(fiName string ) *Classifier {

}*/

// Change the number of buckets for every feature
// where that feature has not already been overridden
func (cl *Classifier) SetNumBuckDefault(nb int16) {
	for a := 0; a < cl.NumCol; a++ {
		feat := cl.ColDef[a]
		if feat.NumBuckOveride == false {
			feat.NumBuck = nb
			cl.updateStepValues(feat)
		}
	}
}

// Set number of buckets for a feature and record
// the fact it's number of buckets has been overridden
// so we don't change it if the default numBuck for
// the entire classifer is changed in the future.
func (cl *Classifier) SetNumBuck(fe *Feature, nb int16) {
	fe.NumBuck = nb
	fe.NumBuckOveride = true
	cl.updateStepValues(fe)
}

func (cl *Classifier) updateStepValues(afeat *Feature) {
	afeat.EffRange = afeat.EffMaxVal - afeat.EffMinVal
	afeat.BuckSize = afeat.EffRange / float32(afeat.NumBuck)
}

// Compute BucketId for current data value for this
// feature.
func (fe *Feature) bucketId(fier *Classifier, dval float32) int16 {
	// TODO: The simple multiplier process works when
	//  the numeric values are gauranteed between 0.0 and 1.0
	//  or are simple integer values.  A better
	//  process  computes a step size based on the value
	//  above minimum value divided by step size. The step size
	//  must be computed based on an effective range with
	//  with outliers removed
	amtOverMin := dval - fe.Spec.MinFlt
	bucket := int16(amtOverMin / float32(fe.BuckSize))
	//fmt.Printf("dval=%v bucket=%v amtOverMin=%v effMinVal=%v\n",
	//	dval, bucket, amtOverMin, fe.EffMinVal)
	return bucket
}

/* separated out the makeEmptyClassifier so
accessible by other construction techniques
like from a string */
func makEmptyClassifier(fiName string, label string, numBuck int16) *Classifier {
	fier := new(Classifier)
	fier.TrainFiName = fiName
	fier.Label = label
	fier.ClassCol = 0
	fier.NumTrainRow = 0
	fier.NumBuck = numBuck
	// can not set ColDef until we know how many
	// colums to allocate space for.
	//fier.ColDef = make([]*Feature, 0fier.Info.NumCol)
	fier.ColByName = make(map[string]*Feature)
	fier.ColNumByName = make(map[string]int)
	fier.ClassCounts = make(map[int16]int32)
	fier.ClassProb = make(map[int16]float32)
	return fier
}

func (fier *Classifier) initFromCSV(csv *CSVInfo) {
	fier.Info = csv
	fier.NumCol = fier.Info.NumCol
	fier.NumRow = fier.Info.NumRow
	fier.ColDef = make([]*Feature, fier.NumCol)
	fier.Info.BuildDistMatrixFile()
	fmt.Println("loadCSVMetadata complete")
	fmt.Println(fier.Info.String())

	// build up the feature description
	// for each column.
	for i := 0; i < fier.NumCol; i++ {
		col := fier.Info.Col[i]
		fier.ColDef[i] = fier.makeFeature(col)
	}
}

func (cl *Classifier) makeFeature(col *CSVCol) *Feature {
	afeat := new(Feature)
	afeat.Spec = col
	afeat.Enabled = true
	afeat.BuckSize = 1
	afeat.EffMaxVal = col.MaxFlt
	afeat.EffMinVal = col.MinFlt
	afeat.ProcType = QTBucket
	afeat.NumBuck = cl.NumBuck
	afeat.NumBuckOveride = false
	afeat.Buckets = make(map[int16]*QuantList)
	afeat.FeatWeight = 1.0
	afeat.NumRow = 0
	afeat.ClassCounts = make(map[int16]int32)
	afeat.ClassProb = make(map[int16]float32)

	cl.updateStepValues(afeat)
	return afeat
}

/* Retrive the class id for the row based on
the column that has been chosen to be used for
class Id */
func (fier *Classifier) classId(vals []string) int16 {
	ctx := vals[fier.ClassCol]
	id, err := strconv.ParseInt(ctx, 10, 16)
	if err != nil {
		//fmt.Printf("classId() Encountered int parsing error val=%v err=%v", ctx, err)
		return -9999
	}
	return int16(id)
}

// Update Class Probability for a given
// row being in any class This is used in
// later probability computations to adjust
// observed probability
func (fier *Classifier) updateClassProb() {

	for classId, classCnt := range fier.ClassCounts {
		fier.ClassProb[classId] = float32(classCnt) / float32(fier.NumTrainRow)
	}

	// TODO: Add  Feature Class Prob
}

/* Train a single feature with a single value for current class
by computing which bucket the value would lie in.  */
func (fier *Classifier) TrainRowFeat(feat *Feature, classId int16, fldVal float32) {
	feat.NumRow += 1
	// Update The Feaure Class Counts
	_, found := feat.ClassCounts[classId]
	if found == false {
		feat.ClassCounts[classId] = 1
	} else {
		feat.ClassCounts[classId] += 1
	}

	// Update the feature Bucket Counts
	buckets := feat.Buckets
	buckId := feat.bucketId(fier, fldVal)
	abuck, bexist := buckets[buckId]
	if bexist == false {
		abuck = new(QuantList)
		abuck.Counts = make(map[int16]int32)
		abuck.totCnt = 0
		buckets[buckId] = abuck
	}
	_, cntExist := abuck.Counts[classId]
	if cntExist == false {
		abuck.Counts[classId] = 1
	} else {
		abuck.Counts[classId] += 1
	}
	abuck.totCnt += 1

	//fmt.Printf("i=%v ctxt=%s f32=%v maxStr=%s minStr=%s\n", i, ctxt, f32, col.MaxStr, col.MinStr)
}

/* Train each feature with data for the current
row.   Figure out which bucket the current
value qualifies for and then update the count for
that class within that bucket. Creats the bucket and
the class if it is the first time we have seen them */
func (fier *Classifier) TrainRow(vals []float32) {
	classCol := fier.ClassCol
	fier.NumTrainRow += 1
	classIdF := vals[classCol] // Update Counts of all rows of
	if classIdF == MaxFloat32 {
		return // this row has invalid class
	}
	classId := int16(classIdF)

	// class so we can compute probability
	// of any row being of a given class
	_, classFnd := fier.ClassCounts[classId]
	if classFnd == false {
		fier.ClassCounts[classId] = 1
	} else {
		fier.ClassCounts[classId] += 1
	}

	for i := 0; i < fier.NumCol; i++ {
		if i == classCol {
			continue // skip the class
		}
		feat := fier.ColDef[i]
		if feat.Enabled == false {
			continue // no need testing disabled features
		}

		fldval := vals[i]
		// We do not actually want to skip columns that have
		// some rows hat can not be parsed because some
		// columns have ocassional data values that are
		// not valid such as ? What we want to do is
		// skip parsing that feature for those rows.
		if fldval == MaxFloat32 {
			continue
		}

		fier.TrainRowFeat(feat, classId, fldval)

	} // for columns
} // func

// Retrains a single feature using the input training
// data supplied in trainArr.
//
// Training by feature may be useful for
// multi-threading since we could theroretically
// use one core per feature since the feature
// level status never affect each other.
func (fier *Classifier) TrainFeature(featNum int16, trainArr [][]float32) {
	feat := fier.ColDef[featNum]
	classCol := fier.ClassCol
	for _, row := range trainArr {
		classIdF := row[classCol]
		if classIdF == MaxFloat32 {
			continue // this row has invalid class
		}
		classId := int16(classIdF)

		fldval := row[featNum]
		if fldval != math.MaxFloat32 {
			fier.TrainRowFeat(feat, classId, fldval)
		}
	} // for
} // func

// Reset the training stats for a given feature
// in preparation for retraining.  This is needed
// for individual features to allow the optimizer
// to only retrain a single feature when it
// changes things.
func (feat *Feature) clearFeatureForRetrain() {
	feat.Buckets = make(map[int16]*QuantList)
	feat.NumRow = 0
}

// Reset the training data to accept new training.
// we will keep the effective range and other meta
// data from the larger data set because this only
// really used by the optimizer
func (fier *Classifier) clearForRetrain() {
	feats := fier.ColDef
	for _, feat := range feats {
		feat.clearFeatureForRetrain()
	}
}

// Throw away current training model dataand
// retrain it with the set of data passed in
// the array rows must contain as many columns
// as there are features.
func (fier *Classifier) Retrain(rows [][]float32) {
	fier.clearForRetrain()
	//classCol := fier.ClassCol
	for _, darr := range rows {
		//classId := int16(darr[classCol])
		fier.TrainRow(darr)
	}
}

// Retrains a given feature.  This is used to support
// the optimizer which is able to change the number of buckets
// for a given feature.
func (fier *Classifier) RetrainFeature(featNdx int16, dataArr [][]float32) {
	feat := fier.ColDef[featNdx]
	feat.clearFeatureForRetrain()
	fier.TrainFeature(featNdx, dataArr)
}

// Load the entire training data file as a simple
// [][]float and return in.   May actually want
// to change the main parser / loader to use this
// for performance since the CSV is currently read
// 3 times and we could load it just once.  But
// if we make that change we still need to be able
// to revert revert to approach that will allow
// training sets larger than physical ram.
func (fier *Classifier) GetTrainRowsAsArr(maxBytes int64) [][]float32 {
	_, rows := LoadCSVRows(fier.TrainFiName, maxBytes)
	return rows
}

// Return an integer array containing all
// currently used class ID
func (fier *Classifier) ClassIds() []int16 {
	cc := fier.ClassCounts
	tout := make([]int16, 0, len(cc))
	for key, _ := range cc {
		tout = append(tout, key)
	}
	return tout
}

func LoadClassifierTrainStream(fier *Classifier, scanner *bufio.Scanner) {
	rowCnt := 0
	scanner.Scan() // skip headers
	for scanner.Scan() {
		txt := s.TrimSpace(scanner.Text())
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
		a := ParseStrAsArrFloat32(txt)
		if len(a) < fier.NumCol {
			continue
		}
		fier.TrainRow(a)
		rowCnt += 1
	} // for row
	fier.updateClassProb()
}

func LoadClassifierTrainFile(fiName string, label string, numBuck int16) *Classifier {
	fmt.Printf("fiName=%s", fiName)

	// Instantiate the basic File Information
	// NOTE: This early construction will have to
	// be duplciates for construction from
	// string when using in POST.
	fier := makEmptyClassifier(fiName, label, numBuck)
	csv := LoadCSVMetaDataFile(fiName)
	fier.initFromCSV(csv)

	// Now process the actual CSV file
	// to do the training work
	file, err := os.Open(fiName)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	// Return the Header and use it's
	// contents to create the columns
	scanner := bufio.NewScanner(file)
	LoadClassifierTrainStream(fier, scanner)
	fier.SetEffMinMaxPortSet(0.015) // remove 1.5% of outlier records from top and bottom
	// when computing range.
	return fier
}

// Function Describe Training Result Finding by Class

// Saves the model wide parameters in INI file key=value
// parameters in file fiName.model.txt  Saves the feature
// defenitions in CSV format.
// featureNum, NumBuck, FeatWeight, TotCnt, Bucket1Id, Bucket1Cnt, Buck1EffMinVal, Buck1MaxVal, Bucket1AbsMinVAl, Bucket1AbsMaxVal
// Where each of the Bucket1* features are repeated for 1..N buckets.
// This should give us everything we need to restore a model with
// all of it's optimized settings intack.  It also gives us a nice
// representation to support the discovery aspects of the system.
// TODO:  Make a brower display utility.
// function SaveModel

// Loads the previously saved model so it can process classification
// results.
// function LoadModel
